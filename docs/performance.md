# Performance

The current implementation favors small process launches and simple parsing.

## Current Characteristics

- Provider requests are executed as subprocesses.
- Streaming is bridged through SSE.
- Health and catalog code are separated from the HTTP request path.

## Future Work

- warm process pools
- catalog caching policies
- health caching
- lower-lock hot paths
- faster model classification
