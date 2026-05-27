FROM golang:1.25-bookworm AS build
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
COPY ["Доп. занятия.xlsx", "/app/seed.xlsx"]
EXPOSE 8080
ENTRYPOINT ["/app/server"]
CMD ["-db", "/data/lessons.db", "-addr", ":8080"]
