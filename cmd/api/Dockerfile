FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -o /out/helios-api ./cmd/api

FROM alpine:3.19
COPY --from=build /out/helios-api /usr/local/bin/helios-api
EXPOSE 8080
ENTRYPOINT ["helios-api"]
