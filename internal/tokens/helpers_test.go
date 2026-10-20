package tokens_test

import "github.com/jackc/pgx/v5/pgtype"

func pgBool(b bool) pgtype.Bool { return pgtype.Bool{Bool: b, Valid: true} }
