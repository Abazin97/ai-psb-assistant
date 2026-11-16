FROM python:3.12-slim

WORKDIR /app
ENV PYTHONUNBUFFERED=1

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY solution.py http_server.py knowledge-base.zip ./

EXPOSE 8000
CMD ["python", "http_server.py"]
