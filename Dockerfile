FROM golang:1.22 AS build

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/media-scout ./cmd/media-scout

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/media-scout /media-scout
EXPOSE 8080
ENTRYPOINT ["/media-scout"]
