// Messing around with the follower graph.

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
	neighbors map[int][]int
	numNodes  int
}

// TODO: it occurs to me that the follower graph should stay directed, and not be
// indirected. Otherwise if you have a lot of followers then you'd see all of their
// posts clearly too. (Or we could just do that. Or change following into a two-way
// "friend" relationship.)
func loadFollowerGraph(conn *sqlite.Conn) (*followerGraph, error) {
	graph := &followerGraph{
		numNodes:  0,
		neighbors: make(map[int][]int),
	}
	query := `
	select user_id as user_id, followed_user_id as other_user_id
	from user_follow

	union

	select followed_user_id as user_id, user_id as other_user_id
	from user_follow
	`
	collect := func(stmt *sqlite.Stmt) error {
		userID1 := stmt.ColumnInt(0)
		userID2 := stmt.ColumnInt(1)
		graph.neighbors[userID1] = append(graph.neighbors[userID1], userID2)
		graph.neighbors[userID2] = append(graph.neighbors[userID2], userID1)
		graph.numNodes = max(graph.numNodes, userID1, userID2)
		return nil
	}
	err := sqlitex.Exec(conn, query, collect)
	return graph, err
}

func depthFirstSearch(graph *followerGraph, start int) {
	// could probably do this with channels in a cool way
	seenDepth := make(map[int]int)
	predecessors := make(map[int]int)
	queue := make([]int, 0)

	queue = append(queue, start)
	seenDepth[start] = 0
	nextQueue := make([]int, 0)
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
	N := graph.numNodes

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
		result.Clone(csr)
	}
	distMat := *sparse.NewDOK(N, N)

	type dist struct {
		d int
		i int
		j int
	}

	distances := make([]dist, 0)

	for depth := 1; depth <= maxDepth; depth++ {
		result.DoNonZero(func(i, j int, v float64) {
			if i >= j {
				return
			}
			if d := distMat.At(i, j); d == 0.0 {
				distMat.Set(i, j, float64(depth))
				distances = append(distances, dist{depth, i, j})
			}
		})
		result.Mul(csr, csr)
		csr.Clone(result)
	}
	for _, triple := range distances {
		fmt.Printf("d=%d | %2d, %2d\n", triple.d, triple.i, triple.j)
	}
}

func main() {
	db, err := entropy.NewDB("test.db", 10)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	conn := db.GetReadOnly(context.Background())
	graph, err := loadFollowerGraph(conn)
	defer db.PutReadOnly(conn)
	if err != nil {
		log.Fatal(err)
	}

	// fmt.Printf("graph: %v\n", graph)
	depthFirstSearch(graph, 18)
	sparseMatrixPowers(graph, 18)
}
