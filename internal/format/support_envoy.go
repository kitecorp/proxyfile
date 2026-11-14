package format

// envoyTarget is Envoy's declaration. The Envoy generator writes every key of the format and both behaviours written
// with no key, so it declares each row of the support table supported.
var envoyTarget = Target{Name: "envoy", Title: "Envoy", Keys: allSupported(MatrixRows())}
