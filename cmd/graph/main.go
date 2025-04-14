package main

import (
	"context"
	"fmt"
	"log"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/james-bowman/sparse"
	"github.com/maxhully/entropy"
)

type followerGraph struct {
	neighbors map[int64][]int64
}

func loadFollowerGraph(conn *sqlite.Conn) (*followerGraph, error) {
	graph := &followerGraph{
		neighbors: make(map[int64][]int64),
	}
	query := `
	select user_id as user_id, followed_user_id as other_user_id
	from user_follow

	union

	select followed_user_id as user_id, user_id as other_user_id
	from user_follow
	`
	collect := func(stmt *sqlite.Stmt) error {
		userID1 := stmt.ColumnInt64(0)
		userID2 := stmt.ColumnInt64(1)
		graph.neighbors[userID1] = append(graph.neighbors[userID1], userID2)
		graph.neighbors[userID2] = append(graph.neighbors[userID2], userID1)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect)
	return graph, err
}

func depthFirstSearch(graph *followerGraph, start int64) {
	// could probably do this with channels in a cool way
	seenDepth := make(map[int64]int)
	predecessors := make(map[int64]int64)
	queue := make([]int64, 0)

	queue = append(queue, start)
	seenDepth[start] = 0
	nextQueue := make([]int64, 0)
	depth := 1
	fmt.Printf("depth: %v\n", depth)
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, n := range graph.neighbors[node] {
			_, alreadySeen := seenDepth[n]
			if alreadySeen {
				continue
			}
			fmt.Printf("n: %v\n", n)
			seenDepth[n] = depth
			predecessors[n] = node
			nextQueue = append(nextQueue, n)
		}
		if len(queue) == 0 {
			queue, nextQueue = nextQueue, queue
			clear(nextQueue)
			depth++
			fmt.Printf("depth: %v\n", depth)
		}
	}
}

func sparseMatrixPowers(graph *followerGraph, maxDepth int) {
	// TODO: actually compute the number of nodes. This can just be len(graph.neighbors)
	// if I change followerGraph to have nodes {0, ... N-1} instead of having the actual
	// user IDs
	N := 27

	var csr *sparse.CSR
	var result *sparse.CSR
	{
		dok := sparse.NewDOK(N, N)
		for k, ns := range graph.neighbors {
			for _, n := range ns {
				dok.Set(int(k)-1, int(n)-1, 1.0)
			}
		}
		csr = dok.ToCSR()
		result = sparse.NewDOK(N, N).ToCSR()
	}

	for depth := 1; depth <= maxDepth; depth++ {
		result.Mul(csr, csr)
		result.DoNonZero(func(i, j int, v float64) {
			// TODO: keep track of seen items
			if i > j {
				return
			}
			fmt.Printf("d=%d|i=%d, j=%d\n", depth, i, j)
		})
		// this won't happen because I've deleted a user from the user table
		if result.NNZ() == N*N {
			fmt.Println("matrix is already saturated! done!")
		}
	}
}

func main() {
	dbpool, err := sqlitex.Open("test.db", 0, 10)
	if err != nil {
		log.Fatal(err)
	}
	defer dbpool.Close()
	db, err := entropy.NewDB(dbpool)
	if err != nil {
		log.Fatal(err)
	}
	conn := db.Get(context.Background())
	defer db.Put(conn)

	graph, err := loadFollowerGraph(conn)
	if err != nil {
		log.Fatal(err)
	}

	// fmt.Printf("graph: %v\n", graph)
	depthFirstSearch(graph, 18)

	sparseMatrixPowers(graph, 6)
}
