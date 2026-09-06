# Migrate the backend to Go behind the existing contracts

Circular will migrate its control plane to Go incrementally, preserving PostgreSQL
records, HTTP/OpenAPI/SSE contracts, the React frontend, and the execution-isolation
guarantees established by M1. The first stage moves worker claiming and expired-claim
recovery to Go while invoking the tested Python execution module once per Run;
Python remains available until the Go execution modules pass the same behavioral
checks, so this bridge is a migration step rather than a completed Go rewrite.
Eino is a separate optional backend decision and does not own Circular's Run
lifecycle, claims, or resource cleanup.
