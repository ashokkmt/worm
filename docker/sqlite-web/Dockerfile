# Lightweight SQLite Web GUI viewer
FROM python:3.11-alpine

# Install sqlite-web and clean cache
RUN pip install --no-cache-dir sqlite-web

# Expose internal web server port
EXPOSE 8080

# Working directory where the SQLite database file is mounted
WORKDIR /data

# Default to running sqlite-web on 0.0.0.0:8080 in read-only mode (-x) against worm.db
ENTRYPOINT ["sqlite_web", "-H", "0.0.0.0", "-p", "8080"]
CMD ["worm.db", "-x"]
