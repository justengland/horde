FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/horde ./cmd/horde

FROM alpine:3.22
RUN apk add --no-cache git ca-certificates
# Committer identity (CommitStages passes --author, but git requires a committer too)
# and safe.directory for the bind-mounted vault (ADR-3).
RUN git config --system user.name horde && \
    git config --system user.email mcp@pluto && \
    git config --system safe.directory /vault
COPY --from=build /out/horde /usr/local/bin/horde
ENV HOME=/tmp
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/horde"]
