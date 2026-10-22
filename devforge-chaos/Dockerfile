FROM python:3.11-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY chaos.py .

# Run unbuffered so Python print() logs show up instantly in Kubernetes
ENV PYTHONUNBUFFERED=1

CMD ["python", "chaos.py"]