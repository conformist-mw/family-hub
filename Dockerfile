FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/import ./cmd/import

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=build /out/import /app/import
# No seed spreadsheet in the image: it holds the family's personal data and
# the public image must not carry it. A fresh install starts empty; to seed
# from Excel, mount the file and run /app/import against it manually.
EXPOSE 8080
ENTRYPOINT ["/app/server"]
CMD ["-db", "/data/family-hub.db", "-addr", ":8080"]
