package persist

import "errors"

// asValidation envuelve errors.As para mantener legible detalleDeValidacion.
func asValidation(err error, destino any) bool {
	return errors.As(err, destino)
}
