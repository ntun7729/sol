FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sol ./cmd/sol

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 sol
COPY --from=build /out/sol /usr/local/bin/sol
USER 10001
ENV PORT=10000
EXPOSE 10000
ENTRYPOINT ["sol"]
CMD ["server"]
