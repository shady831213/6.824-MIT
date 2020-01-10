package shardmaster

//
// Master shard server: assigns shards to replication groups.
//
// RPC interface:
// Join(servers) -- add a set of groups (gid -> server-list mapping).
// Leave(gids) -- delete a set of groups.
// Move(shard, gid) -- hand off one shard from current owner to gid.
// Query(num) -> fetch Config # num, or latest config if num==-1.
//
// A Config (configuration) describes a set of replica groups, and the
// replica group responsible for each shard. Configs are numbered. Config
// #0 is the initial configuration, with no groups and all shards
// assigned to group 0 (the invalid group).
//
// You will need to add fields to the RPC argument structs.
//

// The number of shards.
const NShards = 10

// A configuration -- an assignment of shards to groups.
// Please don't change this.
type Config struct {
	Num    int              // config number
	Shards [NShards]int     // shard -> gid
	Groups map[int][]string // gid -> servers[]
}

const (
	OK = "OK"
)

type Err string

type ArgsBase struct {
	ClerkId int64
	SeqId   int
}


type ReplyBase struct {
	Leader      int
	Server      int
	WrongLeader bool
	Err         Err
}

type JoinArgs struct {
	ArgsBase
	Servers map[int][]string // new GID -> servers mappings
}

type JoinReply struct {
	ReplyBase
}

type LeaveArgs struct {
	ArgsBase
	GIDs []int
}

type LeaveReply struct {
	ReplyBase
}

type MoveArgs struct {
	ArgsBase
	Shard int
	GID   int
}

type MoveReply struct {
	ReplyBase
}

type QueryArgs struct {
	ArgsBase
	Num int // desired config number
}

type QueryReply struct {
	ReplyBase
	Config      Config
}
