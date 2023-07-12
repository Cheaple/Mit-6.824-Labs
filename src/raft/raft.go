package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	//	"bytes"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	//	"6.5840/labgob"
	"6.5840/labrpc"
)


// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 2D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.

// server states
const (
	FOLLOWER = 1
	CANDIDATE = 2
	LEADER = 3
)

// server states
const (
	MaxElapse = 2000		// milliseconds
	MinElapse = 800			// milliseconds
	HeartbeatElapse = 50	// milliseconds
)


type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int

	// For 2D:
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	state		int

	// Persistent state on all servers:
	currentTerm	int		// latest term server has seen (initialized to 0 on first boot, increaes monotonically)
	votedFor	int		// candidatedId that received vote in current term (or null if none)
	logs		[]LogEntry	// log entries

	// Volatile state on all servers:
	commitIndex	int		// index of highest log entry known to be commited (initialized to 0, increaes monotonically)
	lastApplied int		// index of highest log entry applied to state machine (initialized to 0, increaes monotonically)

	// Volatile state on leaders (Reinitialized after election):
	nextIndex	[]int	// for each server, index of the next log entry to send to that server (initialized to leader last log index + 1)
	matchIndex	[]int	// for each server, index of highest log entry known to be replicated on server (initialized to 0, increaes monotonically)

	// Others
	applyCh				chan ApplyMsg
	applierCh			chan int
	electionTimer		*time.Timer
	heartbeatTimer		*time.Timer
	votesGranted		int
}

type LogEntry struct {
	Index	int
	Term	int
	Command interface{}
}

// example RequestVote RPC arguments structure.
// field names must start with capital letters!
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term			int
	CandidateId		int
	LastLogIndex	int
	LastLogTerm		int
}

// example RequestVote RPC reply structure.
// field names must start with capital letters!
type RequestVoteReply struct {
	// Your data here (2A)
	Term		int		// currentTerm, for candidate to update itself
	VoteGranted	bool	// true means candidate received vote
}

type AppendEntriesArgs struct {
	Term			int
	LeaderId		int
	PrevLogIndex	int
	PrevLogTerm		int
	Entries			[]LogEntry
	LeaderCommit	int	
}

type AppendEntriesReply struct {
	Term	int		// current term, for leader to update itself
	Success bool	// true if follower contained entry matching prevLogIndex and prevLogTerm
}



// 2A
// Leader Election
func (rf *Raft) electLeader() {
	rf.mu.Lock()
	rf.currentTerm += 1
	rf.state = CANDIDATE
	rf.votedFor = rf.me  // rf.me never change, so no need to avoid race
	rf.votesGranted = 1
	term := rf.currentTerm  // record currentTerm to avoid race
	log.Printf("Server [%d] started an election at term %d", rf.me, rf.currentTerm)
	rf.mu.Unlock()

	// broadcast RequestVote RPC
	for server, _ := range rf.peers {
		if server == rf.me {
			continue
		}
		go func(server int) {
			log.Printf("Server [%d] send a vote request to server [%d]", rf.me, server)
			args := RequestVoteArgs{
				Term: term,
				CandidateId: rf.me,
				LastLogIndex: rf.lastLogIndex(),
				LastLogTerm: rf.lastLogTerm(),
			}
			var reply RequestVoteReply
			ok := rf.sendRequestVote(server, &args, &reply)
			if !ok {
				return
			}

			// Handle RequestVote RPC response
			rf.mu.Lock()
			defer rf.mu.Unlock()
			if rf.state == CANDIDATE && reply.VoteGranted {
				// Tally votes, if it were still a Candidate
				log.Printf("Server [%d] receive a vote from server [%d]", rf.me, server)
				rf.votesGranted += 1
				if rf.votesGranted > len(rf.peers) / 2{
					log.Printf("Server [%d] becomes LEADER at term %d with %d logs", rf.me, rf.currentTerm, len(rf.logs) - 1)
					rf.state = LEADER
					for i, _ := range rf.peers {
						rf.nextIndex[i] = len(rf.logs)  // reinitialize to last log + 1 (the first log's index is 1, the last log's index is len(logs) - 1)
						rf.matchIndex[i] = 0  // reinitialize to 0
					}
					rf.matchIndex[rf.me] = rf.lastLogIndex()
					rf.broadcastAppendEntries(true)  // heartbeat
				}
			} else if rf.state == CANDIDATE && reply.Term > rf.currentTerm {
				// log.Printf("Server [%d] find a new leader server [%d] in term %d", rf.me, server, reply.Term)
				rf.state = FOLLOWER
				rf.currentTerm = reply.Term
			}
		}(server)
	}
}

func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

// Handler for RequestVote RPC 
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	log.Printf("Server [%d] (term %d) received an vote request from server [%d] at term %d", rf.me, rf.currentTerm, args.CandidateId, args.Term)
	log.Printf("    Args: {Candidate: %d, Term: %d, LastLogIndex: %d, LastLogTerm: %d}", args.CandidateId, args.Term, args.LastLogIndex, args.LastLogTerm)
	reply.Term = rf.currentTerm
	reply.VoteGranted = false

	if rf.currentTerm < args.Term || rf.votedFor == -1|| rf.votedFor == args.CandidateId {
		// if candidate’s log is not up-to-date as receiver’s log, reply false (according to Figure 2.3.3.2)
		if rf.lastLogTerm() > args.LastLogTerm {
			return
		} else if rf.lastLogTerm() == args.LastLogTerm && rf.lastLogIndex() > args.LastLogIndex {
			return
		}
		reply.VoteGranted = true
		rf.currentTerm = args.Term
		rf.votedFor = args.CandidateId
		rf.state = FOLLOWER
		rf.resetElectionTimer()
	}
}



// 2B
// Log
func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

//
// Broadcast AppendEntries RPC to all servers
//
func (rf *Raft) broadcastAppendEntries(heartbeat bool) {
	for server, _ := range rf.peers {
		if server == rf.me {
			continue
		}
		go rf.sendAppendEntriesRequest(server, heartbeat)
	}
}

//
// Send a AppendEntries RPC to a single server
//
func (rf *Raft) sendAppendEntriesRequest(server int, heartbeat bool) {
	rf.mu.Lock()
	args := &AppendEntriesArgs{
		Term: rf.currentTerm,
		LeaderId: rf.me,
		PrevLogIndex: rf.nextIndex[server] - 1,  // index of log entry immediately preceding new ones
		PrevLogTerm: rf.logs[rf.nextIndex[server] - 1].Term,
		LeaderCommit: rf.commitIndex, 
	}
	if !heartbeat {
		args.Entries = rf.logs[rf.nextIndex[server]:]
	}
	reply := &AppendEntriesReply{}
	rf.mu.Unlock()
	ok := rf.sendAppendEntries(server, args, reply)
	if !ok {
		return
	}

	// Handle AppendEntries response (according to Figure 2.4.4.3)
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if reply.Success {
		// If successful: update nextIndex and matchIndex for follower
		// log.Printf("Server [%d] receive a success from server [%d]; update nextIndex[%d] from %d to %d", rf.me, server, server, rf.nextIndex[server], args.PrevLogIndex + len(args.Entries) + 1)
		rf.nextIndex[server] = args.PrevLogIndex + len(args.Entries) + 1
		rf.matchIndex[server] = args.PrevLogIndex + len(args.Entries)
		log.Printf("Leader [%d] (term %d)", rf.me, rf.currentTerm)
		log.Printf("    nextIndex: %#v", rf.nextIndex)
		log.Printf("    matchIndex: %#v", rf.matchIndex)
		
		if rf.commitIndex < rf.matchIndex[server] {  // if the newest replicated entry has not been committed
			rf.tryCommit(rf.matchIndex[server])  // try to commit it
		}
	} else if !reply.Success && reply.Term <= args.Term {
		// If AppendEntries fails because of log inconsistency: decrement nextIndex and retry
		rf.nextIndex[server] -= 1
		go rf.sendAppendEntriesRequest(server, heartbeat)
	}  
}


// Commit a log
func (rf *Raft) tryCommit(logIndex int) {
	// rf.mu.Lock()
	// defer rf.mu.Unlock()
	if rf.logs[logIndex].Term != rf.currentTerm {
		return  // according to Figure 2.4.4.4
	}
	cnt := 0  // count the number of this log entry' replications on all servers
	for _, matchIdx := range rf.matchIndex {
		if matchIdx >= logIndex {
			cnt += 1 
		}
	}
	log.Printf("    log %d has been replicated to %d servers.", logIndex, cnt)
	if cnt > len(rf.peers) / 2 {
		// if the log entry has been replicated it on a majority of the servers
		rf.commitIndex = logIndex  // commit it
		log.Printf("Leader [%d] (term %d) commits log %d", rf.me, rf.currentTerm, logIndex)
	}
	rf.applierCh <- 1
	// Then, newly committed log entries will be applied to the client asynchronously
}


// Handler for AppendEntries RPC
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	log.Printf("Server [%d] (term %d) received an AppendEntries from server [%d] at term %d", rf.me, rf.currentTerm, args.LeaderId, args.Term)
	log.Printf("    Args: {Leader: %d, Term: %d, PrevLogIndex: %d, PrevLogTerm: %d, new entries count: %d}", args.LeaderId, args.Term, args.PrevLogIndex, args.PrevLogTerm, len(args.Entries))
	reply.Term = rf.currentTerm
	reply.Success = true
	if rf.currentTerm > args.Term {
		reply.Success = false  // Reply false if term < currentTerm (according to Figure 2.2.3.1)
		return
	} 

	// If the leader’s term (included in its RPC) is at least as large as the candidate’s current term
	// recognizes the leader as legitimate
	rf.currentTerm = args.Term
	rf.state = FOLLOWER  // returns to follower state
	rf.resetElectionTimer()

	// Consistency Check
	if args.PrevLogIndex >= len(rf.logs) || rf.logs[args.PrevLogIndex].Term != args.PrevLogTerm {
		//  if log doesn’t contain an entry at prevLogIndex whose term matches prevLogTerm,
		reply.Success = false  // reply false (according to Figure 2.2.3.2)
		return 
	}

	// Append new entires
	if args.Entries != nil {  
		rf.logs = append(rf.logs[:args.PrevLogIndex + 1], args.Entries...)
	}

	// Apply newly committed log enties
	if args.LeaderCommit > rf.commitIndex {
		// set commitIndex = min(leaderCommit, index of last new entry (according to Figure 2.2.3.5)
		if args.LeaderCommit < rf.lastLogIndex() {
			rf.commitIndex = args.LeaderCommit
		} else {
			rf.commitIndex = rf.lastLogIndex()
		}
		rf.applierCh <- 1
	}

}

func (rf *Raft) lastLogIndex() int {
	return len(rf.logs) - 1
}

func (rf *Raft) lastLogTerm() int {
	return rf.logs[len(rf.logs) - 1].Term
}



// 2C
// Persistence
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
// before you've implemented snapshots, you should pass nil as the
// second argument to persister.Save().
// after you've implemented snapshots, pass the current snapshot
// (or nil if there's not yet a snapshot).
func (rf *Raft) persist() {
	// Your code here (2C).


	// Example:
	// w := new(bytes.Buffer)
	// e := labgob.NewEncoder(w)
	// e.Encode(rf.xxx)
	// e.Encode(rf.yyy)
	// raftstate := w.Bytes()
	// rf.persister.Save(raftstate, nil)
}

// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (2C).
	// Example:
	// r := bytes.NewBuffer(data)
	// d := labgob.NewDecoder(r)
	// var xxx
	// var yyy
	// if d.Decode(&xxx) != nil ||
	//    d.Decode(&yyy) != nil {
	//   error...
	// } else {
	//   rf.xxx = xxx
	//   rf.yyy = yyy
	// }
}



// 2D
// Log Compaction
// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).

}

// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := true

	// Your code here (2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	index = rf.commitIndex + 1
	term = rf.currentTerm
	isLeader = (rf.state == LEADER)

	if isLeader {
		rf.logs = append(rf.logs, LogEntry{
			Index: len(rf.logs),
			Term: rf.currentTerm,
			Command: command,
		})
		rf.matchIndex[rf.me] = rf.lastLogIndex()
		log.Printf("Leader [%d] (term %d) get a new commmand with an index %d. Now it has %d logs", rf.me, rf.currentTerm, rf.lastLogIndex(), len(rf.logs) - 1)
	}
	

	return index, term, isLeader
}

// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

func (rf *Raft) ticker() {
	for rf.killed() == false {

		// Your code here (2A)
		// Check if a leader election should be started.
		select {
		case <- rf.electionTimer.C:
			if rf.state != LEADER { 
				rf.electLeader()
			}
			rf.resetElectionTimer()
		case <- rf.heartbeatTimer.C:
			if rf.state == LEADER {
				rf.broadcastAppendEntries(false)
			}
			rf.resetHeartbeatTimer()
		default:
		}

		// pause for a random amount of time between 50 and 350
		// milliseconds.
		ms := 50 + (rand.Int63() % 300)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

// Apply committed log entries to the client asynchronously (the process is slow)
func (rf *Raft) applier() {
	for rf.killed() == false {
		<- rf.applierCh
		rf.mu.Lock()
		if rf.lastApplied < rf.commitIndex {
			for _, entry := range rf.logs[(rf.lastApplied + 1) : (rf.commitIndex + 1)] {
				rf.applyCh <- ApplyMsg {
					Command: entry.Command,
					CommandIndex: entry.Index,
					CommandValid: true,
				}
				log.Printf("Server [%d] (term %d) applied log entry %d", rf.me, rf.currentTerm, entry.Index)
			}

		}
		rf.lastApplied = rf.commitIndex
		rf.mu.Unlock()
	}

}


func (rf *Raft)resetElectionTimer() {
	elapse := int64(MinElapse) + int64(rand.Intn(MaxElapse - MinElapse))
	rf.electionTimer.Reset(time.Duration(elapse) * time.Millisecond)
}

func (rf *Raft)resetHeartbeatTimer() {
	rf.heartbeatTimer.Reset(time.Duration(HeartbeatElapse) * time.Millisecond)
} 


// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {

	var term int
	var isleader bool
	// Your code here (2A).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	term = rf.currentTerm
	isleader = (rf.state == LEADER)

	return term, isleader
}

// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (2A, 2B, 2C).
	rf.state = FOLLOWER
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.logs = make([]LogEntry, 1)
	rf.commitIndex = 0
	rf.lastApplied = 0
	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))

	rf.applyCh = applyCh
	rf.electionTimer = time.NewTimer(time.Millisecond * 200)
	rf.resetElectionTimer()
	rf.heartbeatTimer = time.NewTimer(time.Millisecond * 200)
	
	rf.applierCh = make(chan int)
	go rf.applier()

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections
	go rf.ticker()


	return rf
}
