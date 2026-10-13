# The browser tests run in Playwright's own image (a browser and its system libraries), so no host needs them installed.
# Keep the tag in step with the playwright version it ships.
FROM mcr.microsoft.com/playwright/python:v1.63.0-noble
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt
WORKDIR /hub/e2e
