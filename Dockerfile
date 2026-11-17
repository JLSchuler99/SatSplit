FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
ARG TARGETOS TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/satsplit ./cmd/satsplit

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
     ca-certificates \
     tesseract-ocr \
     tesseract-ocr-eng \
     tesseract-ocr-spa \
     tesseract-ocr-fra \
     tesseract-ocr-deu \
     tesseract-ocr-hun \
     tesseract-ocr-por \
     tesseract-ocr-jpn \
     tesseract-ocr-kor \
     tesseract-ocr-chi-sim \
     tesseract-ocr-tha \
  && rm -rf /var/lib/apt/lists/*

# heif-convert turns iPhone HEIC photos into JPEG. The bookworm version (1.15)
# cannot read files from recent iPhones, so take libheif from backports.
RUN echo "deb http://deb.debian.org/debian bookworm-backports main" > /etc/apt/sources.list.d/backports.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends -t bookworm-backports \
     libheif-examples \
     libheif-plugin-libde265 \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/satsplit /app/satsplit

ENV SAT_SPLIT_ADDR=:8080 \
    SAT_SPLIT_DB=/data/satsplit.db \
    SAT_SPLIT_UPLOAD_DIR=/data/uploads \
    TESSERACT_CMD=tesseract \
    TESSERACT_LANG=eng

EXPOSE 8080
VOLUME ["/data"]

CMD ["/app/satsplit"]
