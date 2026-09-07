# Bench Hire

default: check

run: build
    ./hire

check:
    go vet ./...
    go test ./...
    node --check web/app.js
    node --check web/connections.js

build:
    go build -o hire .
