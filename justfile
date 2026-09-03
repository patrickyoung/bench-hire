# Bench Hire

default: check

run:
    go run .

check:
    go vet ./...
    go test ./...
    node --check web/app.js

build:
    go build -o hire .
