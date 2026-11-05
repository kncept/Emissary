

The core 'serve a web client' is universal.
The source and sink are pluggable, driven by Env Vars.
Note that the sender (outbound) and the storage (persistence) must be individually specificable.
If no metadata is specified, then the memory implementation will be defaulted to.

The react GUI is served from the same domain/port as the HTTP API lives on.
All the api calls are nested under an `api` url prefix