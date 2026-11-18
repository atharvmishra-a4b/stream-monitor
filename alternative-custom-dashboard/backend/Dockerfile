FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /backend .

FROM alpine:3.20
WORKDIR /app
COPY --from=build /backend /app/backend
COPY seed /app/seed
EXPOSE 8080
CMD ["/app/backend"]
