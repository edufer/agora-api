package ballotbox

import (
	"github.com/agoravoting/agora-http-go/middleware"
	s "github.com/agoravoting/agora-http-go/server"
	"github.com/agoravoting/agora-http-go/util"
	"github.com/codegangsta/negroni"
	"github.com/jmoiron/sqlx"
	"github.com/julienschmidt/httprouter"
	// 	"net/http/httputil"
	"encoding/json"
	"net/http"
	"fmt"
)

const (
	SESSION_EXPIRE = 3600
)

type BallotBox struct {
	router *httprouter.Router
	name   string

	insertStmt *sqlx.NamedStmt
	getStmt    *sqlx.Stmt
	delStmt    *sqlx.Stmt
}

func (bb *BallotBox) Name() string {
	return bb.name
}

func (bb *BallotBox) Init() (err error) {
	// setup the routes
	bb.router = httprouter.New()
	bb.router.POST("/:election_id/:voter_id", middleware.Join(
		s.Server.ErrorWrap.Do(bb.post),
		s.Server.CheckPerms("voter-${election_id}-${voter_id}", SESSION_EXPIRE)))
	bb.router.GET("/:election_id/:voter_id/:vote_hash", middleware.Join(
		s.Server.ErrorWrap.Do(bb.get),
		s.Server.CheckPerms("voter-${election_id}-${voter_id}", SESSION_EXPIRE)))

	// setup prepared sql queries
	if bb.insertStmt, err = s.Server.Db.PrepareNamed("INSERT INTO votes (vote, vote_hash, election_id, voter_id) VALUES (:vote, :vote_hash, :election_id, :voter_id) RETURNING id"); err != nil {
		return
	}
	if bb.getStmt, err = s.Server.Db.Preparex("SELECT * FROM votes WHERE election_id = $1 and voter_id = $2 and vote_hash = $3"); err != nil {
		return
	}

	// add the routes to the server
	handler := negroni.New(negroni.Wrap(bb.router))
	s.Server.Mux.OnMux("api/v1/ballotbox", handler)
	return
}

// lists the available events
func (bb *BallotBox) list(w http.ResponseWriter, r *http.Request, _ httprouter.Params) *middleware.HandledError {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Hello world!"))
	return nil
}

// returns an event
func (bb *BallotBox) get(w http.ResponseWriter, r *http.Request, p httprouter.Params) *middleware.HandledError {
	var (
		v   []Vote
		err error
		voteHash string
	)

	electionId := p.ByName("election_id")
	voterId := p.ByName("voter_id")
	voteHash = p.ByName("vote_hash")
	if electionId == "" {
		return &middleware.HandledError{Err: err, Code: 400, Message: "No election_id", CodedMessage: "error-insert"}
	}
	if voterId == "" {
		return &middleware.HandledError{Err: err, Code: 400, Message: "No voter_id", CodedMessage: "error-insert"}
	}
	if voteHash == "" {
		return &middleware.HandledError{Err: err, Code: 400, Message: "Invalid hash format", CodedMessage: "invalid-format"}
	}

	if err = bb.getStmt.Select(&v, electionId, voterId, voteHash); err != nil {
		return &middleware.HandledError{Err: err, Code: 500, Message: "Database error", CodedMessage: "error-select"}
	}

	if len(v) == 0 {
		return &middleware.HandledError{Err: err, Code: 404, Message: "Not found", CodedMessage: "not-found"}
	}

	b, err := v[0].Marshal()
	if err != nil {
		return &middleware.HandledError{Err: err, Code: 500, Message: "Error marshalling the data", CodedMessage: "marshall-error"}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
	return nil
}
/*
func (bb *BallotBox) delete(w http.ResponseWriter, r *http.Request, p httprouter.Params) *middleware.HandledError {
	var (
		e   []Event
		err error
		id  int64
	)

	id, err = strconv.ParseInt(p.ByName("id"), 10, 32)
	if err != nil || id <= 0 {
		return &middleware.HandledError{Err: err, Code: 400, Message: "Invalid id format", CodedMessage: "invalid-format"}
	}

	if err = bb.getStmt.Select(&e, id); err != nil {
		return &middleware.HandledError{Err: err, Code: 500, Message: "Database error", CodedMessage: "error-select"}
	}

	if len(e) == 0 {
		return &middleware.HandledError{Err: err, Code: 404, Message: "Not found", CodedMessage: "not-found"}
	}

	if _, err := bb.delStmt.Exec(id); err != nil {
		return &middleware.HandledError{Err: err, Code: 500, Message: "Error deleting the data", CodedMessage: "sql-error"}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return nil
}
*/
// parses an event from a request.
func parseVote(r *http.Request) (v Vote, err error) {
	// 	rb, err := httputil.DumpRequest(r, true)
	if err != nil {
		return
	}
	decoder := json.NewDecoder(r.Body)
	err = decoder.Decode(&v)
	if err != nil {
		return
	}
	return
}

// add a new event
func (bb *BallotBox) post(w http.ResponseWriter, r *http.Request, p httprouter.Params) *middleware.HandledError {
	var (
		tx    = s.Server.Db.MustBegin()
		vote  Vote
		id    int
		err   error
	)
	vote, err = parseVote(r)
	if err != nil {
		return &middleware.HandledError{Err: err, Code: 400, Message: "Invalid json-encoded vote", CodedMessage: "invalid-json"}
	}
	vote_json, err := vote.Json()
	if err != nil {
		return &middleware.HandledError{Err: err, Code: 500, Message: "Error re-writing the data to json", CodedMessage: "error-json-encode"}
	}

	electionId := p.ByName("election_id")
	voterId := p.ByName("voter_id")
	if electionId == "" {
		return &middleware.HandledError{Err: err, Code: 400, Message: "No election_id", CodedMessage: "error-insert"}
	}
	if voterId == "" {
		return &middleware.HandledError{Err: err, Code: 400, Message: "No voter_id", CodedMessage: "error-insert"}
	}
	vote_json["election_id"] = electionId
	vote_json["voter_id"] = voterId

	if err = tx.NamedStmt(bb.insertStmt).QueryRowx(vote_json).Scan(&id); err != nil {
		tx.Rollback()
		return &middleware.HandledError{Err: err, Code: 500, Message: fmt.Sprintf("Error inserting the vote: %s", err), CodedMessage: "error-insert"}
	}
	err = tx.Commit()
	if err != nil {
		tx.Rollback()
		return &middleware.HandledError{Err: err, Code: 500, Message: "Error comitting the vote", CodedMessage: "error-commit"}
	}

	// return id
	if err = util.WriteIdJson(w, id); err != nil {
		return &middleware.HandledError{Err: err, Code: 500, Message: "Error returing the id", CodedMessage: "error-return"}
	}
	return nil
}

// add the modules to available modules on startup
func init() {
	s.Server.AvailableModules = append(s.Server.AvailableModules, &BallotBox{name: "github.com/agoravoting/agora-api/ballotbox"})
}