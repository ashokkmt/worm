package decode

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"worm/internal/model"
)

// XMLDecoder decodes XML documents into one or more DecodedRecords with strict
// structural bounds and XXE prevention (DOCTYPE/directives rejection).
type XMLDecoder struct{}

// NewXMLDecoder creates a new XML format decoder.
func NewXMLDecoder() *XMLDecoder {
	return &XMLDecoder{}
}

// Name returns the canonical format name "xml".
func (d *XMLDecoder) Name() string {
	return "xml"
}

// Detect checks for XML declaration or root element framing.
func (d *XMLDecoder) Detect(raw []byte) float64 {
	trimmed := bytes.TrimSpace(stripBOM(raw))
	if len(trimmed) == 0 {
		return 0.0
	}

	// High confidence for explicit XML declaration
	if bytes.HasPrefix(trimmed, []byte("<?xml")) {
		return 0.95
	}

	// Tag starting with < followed by valid identifier and ending with >
	if trimmed[0] == '<' && !bytes.HasPrefix(trimmed, []byte("<?")) && !bytes.HasPrefix(trimmed, []byte("<!--")) {
		// Avoid matching syslog PRI e.g. <14>
		if len(trimmed) > 1 && trimmed[1] >= '0' && trimmed[1] <= '9' {
			return 0.0
		}
		// Look for closing root tag
		endTag := bytes.IndexByte(trimmed, '>')
		if endTag > 1 {
			tagName := string(trimmed[1:endTag])
			fields := strings.Fields(tagName)
			if len(fields) > 0 {
				root := fields[0]
				// Root must start with letter
				if len(root) > 0 && ((root[0] >= 'a' && root[0] <= 'z') || (root[0] >= 'A' && root[0] <= 'Z')) {
					// Check if document closes with matching or self-closing tag
					if bytes.HasSuffix(trimmed, []byte(">")) {
						return 0.90
					}
					return 0.60
				}
			}
		}
	}

	return 0.0
}

type xmlNode struct {
	Name       xml.Name
	Attrs      map[string]string
	Text       string
	Children   []*xmlNode
	ChildOrder []string
	RawBytes   []byte
}

// Decode parses the raw XML document into child DecodedRecords.
func (d *XMLDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("payload size %d exceeds maximum allowed %d bytes", len(raw), MaxPayloadBytes)
	}

	cleaned := stripBOM(raw)
	trimmed := bytes.TrimSpace(cleaned)
	if len(trimmed) == 0 {
		return nil, errors.New("empty XML payload")
	}

	dec := xml.NewDecoder(bytes.NewReader(trimmed))
	dec.Strict = true
	dec.Entity = nil // No external entity resolution

	var (
		rootNode   *xmlNode
		stack      []*xmlNode
		tokenCount int
	)

	for {
		tokenCount++
		if tokenCount > 50000 {
			return nil, errors.New("token limit exceeded: XML contains more than 50,000 tokens")
		}

		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("xml syntax error: %w", err)
		}

		switch t := tok.(type) {
		case xml.Directive:
			// XXE protection: reject DOCTYPE and any custom directives
			dirStr := strings.ToUpper(string(t))
			if strings.Contains(dirStr, "DOCTYPE") || strings.Contains(dirStr, "ENTITY") {
				return nil, errors.New("security violation: XML DOCTYPE and ENTITY directives are forbidden in log inputs")
			}

		case xml.StartElement:
			if len(stack) >= MaxDepth {
				return nil, fmt.Errorf("xml depth limit exceeded: depth > %d", MaxDepth)
			}

			attrs := make(map[string]string, len(t.Attr))
			for _, a := range t.Attr {
				attrKey := "@" + a.Name.Local
				if a.Name.Space != "" {
					attrKey = fmt.Sprintf("@%s:%s", a.Name.Space, a.Name.Local)
				}
				attrs[attrKey] = a.Value
			}

			node := &xmlNode{
				Name:  t.Name,
				Attrs: attrs,
			}

			if rootNode == nil {
				rootNode = node
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
				parent.ChildOrder = append(parent.ChildOrder, node.Name.Local)
			}

			stack = append(stack, node)

		case xml.CharData:
			if len(stack) > 0 {
				str := string(t)
				if strings.TrimSpace(str) != "" {
					curr := stack[len(stack)-1]
					curr.Text += str
				}
			}

		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected closing tag </%s>", t.Name.Local)
			}
			stack = stack[:len(stack)-1]
		}
	}

	if rootNode == nil {
		return nil, errors.New("no root XML element found")
	}

	// Extract records:
	// If root contains multiple children of the same element name or multiple child elements,
	// treat each child as a batch record (e.g. <events><event>...</event></events>).
	// If root has no children or represents a single event, return root as 1 record.
	var records []*model.DecodedRecord

	headers := map[string]any{
		"xml_root_local": rootNode.Name.Local,
	}
	if rootNode.Name.Space != "" {
		headers["xml_root_namespace"] = rootNode.Name.Space
	}
	for k, v := range rootNode.Attrs {
		headers[k] = v
	}

	isBatch := len(rootNode.Children) > 0 && isCollectionRoot(rootNode)

	if isBatch {
		for i, child := range rootNode.Children {
			if i >= MaxChildren {
				return nil, fmt.Errorf("batch child count exceeded maximum %d", MaxChildren)
			}

			childFields := nodeToMap(child)
			// Merge root attributes into fields if not present
			for k, v := range headers {
				if _, ok := childFields[k]; !ok {
					childFields[k] = v
				}
			}

			records = append(records, &model.DecodedRecord{
				RecordOrdinal: i,
				Format:        "xml",
				Headers:       headers,
				Fields:        childFields,
				RawPayload:    raw,
			})
		}
	} else {
		// Single record from root
		rootFields := nodeToMap(rootNode)
		records = append(records, &model.DecodedRecord{
			RecordOrdinal: 0,
			Format:        "xml",
			Headers:       headers,
			Fields:        rootFields,
			RawPayload:    raw,
		})
	}

	return records, nil
}

func isCollectionRoot(node *xmlNode) bool {
	if len(node.Children) == 0 {
		return false
	}
	// If root is plural name like "events", "logs", "records", "items", "telemetry_batch"
	local := strings.ToLower(node.Name.Local)
	if strings.HasSuffix(local, "s") || strings.HasSuffix(local, "list") || strings.HasSuffix(local, "batch") {
		return true
	}
	// If all children have the same element name (e.g. <event>, <log>, <record>)
	firstName := node.Children[0].Name.Local
	for _, c := range node.Children {
		if c.Name.Local != firstName {
			return false
		}
	}
	return true
}

func nodeToMap(node *xmlNode) map[string]any {
	result := make(map[string]any)

	// Add attributes
	for k, v := range node.Attrs {
		result[k] = v
	}

	// Add text if present
	trimmedText := strings.TrimSpace(node.Text)
	if trimmedText != "" {
		result["#text"] = trimmedText
	}

	// Group children by element name
	childGroups := make(map[string][]*xmlNode)
	for _, c := range node.Children {
		childGroups[c.Name.Local] = append(childGroups[c.Name.Local], c)
	}

	for name, group := range childGroups {
		if len(group) == 1 {
			c := group[0]
			if len(c.Children) == 0 && len(c.Attrs) == 0 {
				// Pure leaf text node
				result[name] = strings.TrimSpace(c.Text)
			} else {
				subMap := nodeToMap(c)
				result[name] = subMap
			}
		} else {
			// Array of children
			var list []any
			for _, c := range group {
				if len(c.Children) == 0 && len(c.Attrs) == 0 {
					list = append(list, strings.TrimSpace(c.Text))
				} else {
					list = append(list, nodeToMap(c))
				}
			}
			result[name] = list
		}
	}

	return result
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
