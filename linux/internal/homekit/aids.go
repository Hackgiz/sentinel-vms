package homekit

import (
	"context"
	"database/sql"
)

// bridgeAID is the bridge accessory's id; cameras start at 2.
const bridgeAID = 1

// aidRegistry hands out HomeKit accessory ids. A camera gets the next free
// id the first time it is exposed and keeps it for good: ids are never
// reused or renumbered, because a controller caches by id and a recycled one
// would show a new camera as the old one. Deleting a camera keeps its row,
// so the next camera still gets a fresh number.
type aidRegistry struct{ db *sql.DB }

func newAIDRegistry(db *sql.DB) *aidRegistry { return &aidRegistry{db: db} }

// Assign returns the camera's accessory id, allocating one if it has none.
func (r *aidRegistry) Assign(ctx context.Context, cameraID string) (uint64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var aid uint64
	err = tx.QueryRowContext(ctx, `SELECT aid FROM homekit_aids WHERE camera_id = ?`, cameraID).Scan(&aid)
	if err == sql.ErrNoRows {
		// MAX over every row ever written, deleted cameras included.
		if _, err = tx.ExecContext(ctx, `INSERT INTO homekit_aids(camera_id, aid)
			SELECT ?, MAX(COALESCE(MAX(aid), 0), ?) + 1 FROM homekit_aids`, cameraID, bridgeAID); err != nil {
			return 0, err
		}
		err = tx.QueryRowContext(ctx, `SELECT aid FROM homekit_aids WHERE camera_id = ?`, cameraID).Scan(&aid)
	}
	if err != nil {
		return 0, err
	}
	return aid, tx.Commit()
}
