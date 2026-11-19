package apperr

type Invalid struct{ Message string }

func (e Invalid) Error() string { return e.Message }

type NotFound struct{ Message string }

func (e NotFound) Error() string { return e.Message }

type Conflict struct{ Message string }

func (e Conflict) Error() string { return e.Message }

type Busy struct{ Message string }

func (e Busy) Error() string { return e.Message }
