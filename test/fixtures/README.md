# Fixtures

* `cross-file-refs/`: a service spec that references `../common/openapi.yml` for a header parameter and two schemas. Imported unbundled, Bruno drops the header and empties the body fields. Bundled, everything is present (DESIGN.md section 2).
* `missing-operationid/`: the same spec with all operationIds removed. Must fail the producer lint, and brunosync falls back to "METHOD path" identity with a warning.
