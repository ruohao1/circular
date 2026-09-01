# API contracts

FastAPI's OpenAPI document is the authoritative HTTP contract. During development it is
available at `http://localhost:8000/openapi.json` (interactive documentation is at
`/docs`). A generated TypeScript client will be added from that document when the UI
begins consuming resource endpoints; handwritten duplicate contract models should not
be added here.
