FROM python:3.11-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY *.py ./

# Kill switch lives here: `touch /var/run/chaos/pause` pauses all experiments. Reports go to /reports.
RUN mkdir -p /var/run/chaos /reports

# Run unbuffered so Python logs show up instantly in Kubernetes / docker logs
ENV PYTHONUNBUFFERED=1

CMD ["python", "chaos.py"]
