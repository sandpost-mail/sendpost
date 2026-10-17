package auth_test

import "github.com/jackc/pgx/v5/pgtype"

func boolVal(b bool) pgtype.Bool { return pgtype.Bool{Bool: b, Valid: true} }
