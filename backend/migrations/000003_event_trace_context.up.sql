-- W3C traceparent of the ingestion request, so the worker's delivery spans join the
-- same trace as the API request that created the event. NULL when tracing is off.
ALTER TABLE events ADD COLUMN trace_context text CHECK (char_length(trace_context) <= 128);
