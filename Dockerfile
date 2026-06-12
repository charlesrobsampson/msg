FROM golang:1.25-alpine
RUN apk add --no-cache git
WORKDIR /app
RUN git clone https://github.com/MaxGhenis/openmessage.git .
# Patch the hardcoded bind address to allow Docker mapping
RUN find . -type f -name "*.go" -exec sed -i 's/127.0.0.1/0.0.0.0/g' {} +
RUN go build -o openmessage .
EXPOSE 7007
CMD ["./openmessage", "serve"]
