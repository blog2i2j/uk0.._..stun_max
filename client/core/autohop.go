package core

import (
	"encoding/json"
	"fmt"
)

// broadcastP2PMap sends our current P2P connectivity map to all peers in the room.
// Called when a hole punch succeeds or during periodic retry rounds.
func (c *Client) broadcastP2PMap() {
	// Only include peers that are in the current peer list AND have direct mode.
	// This prevents stale PeerConn entries (from reconnected peers with old IDs)
	// from being included in the P2P map broadcast.
	activePeers := make(map[string]bool)
	c.peersMu.RLock()
	for _, p := range c.peers {
		activePeers[p.ID] = true
	}
	c.peersMu.RUnlock()

	var directPeers []string
	c.peerConnsMu.RLock()
	for id, pc := range c.peerConns {
		if pc.Mode == "direct" && activePeers[id] {
			directPeers = append(directPeers, id)
		}
	}
	c.peerConnsMu.RUnlock()

	msg := P2PMap{DirectPeers: directPeers}

	c.peersMu.RLock()
	peers := make([]string, 0, len(c.peers))
	for _, p := range c.peers {
		if p.ID != c.MyID {
			peers = append(peers, p.ID)
		}
	}
	c.peersMu.RUnlock()

	for _, pid := range peers {
		c.sendRelay(pid, "p2p_map", msg)
	}
}

// handleP2PMap processes an incoming P2P connectivity map from a peer.
func (c *Client) handleP2PMap(msg Message) {
	var pmap P2PMap
	if err := json.Unmarshal(msg.Payload, &pmap); err != nil {
		return
	}

	directSet := make(map[string]bool, len(pmap.DirectPeers))
	for _, id := range pmap.DirectPeers {
		directSet[id] = true
	}

	c.p2pMapsMu.Lock()
	c.p2pMaps[msg.From] = directSet
	c.p2pMapsMu.Unlock()
}

// findAutoHopCandidate searches for a peer that has direct P2P connections to
// both us and the target peer, making it a viable hop relay.
// Returns the candidate peer ID, or "" if none found.
func (c *Client) findAutoHopCandidate(targetPeerID string) string {
	c.p2pMapsMu.RLock()
	defer c.p2pMapsMu.RUnlock()

	var bestCandidate string
	bestScore := -1

	for candidateID, directPeers := range c.p2pMaps {
		if candidateID == targetPeerID || candidateID == c.MyID {
			continue
		}
		// Candidate must have direct P2P to target
		if !directPeers[targetPeerID] {
			continue
		}
		// We must have direct P2P to candidate
		c.peerConnsMu.RLock()
		pc, ok := c.peerConns[candidateID]
		c.peerConnsMu.RUnlock()
		if !ok || pc.Mode != "direct" {
			continue
		}

		// Score: prefer encrypted channel, then lower PunchFails
		score := 100
		if pc.Crypto != nil && pc.Crypto.IsEncrypted() {
			score += 50
		}
		score -= pc.PunchFails
		if score > bestScore {
			bestScore = score
			bestCandidate = candidateID
		}
	}
	return bestCandidate
}

// tryAutoHop attempts to find and record an auto-hop candidate for a target peer.
// Called when hole punch fails and mode transitions to "relay".
func (c *Client) tryAutoHop(targetPeerID string) {
	// Check if already has auto-hop
	c.peerConnsMu.RLock()
	pc, ok := c.peerConns[targetPeerID]
	if ok && pc.AutoHopVia != "" {
		c.peerConnsMu.RUnlock()
		return
	}
	c.peerConnsMu.RUnlock()

	candidate := c.findAutoHopCandidate(targetPeerID)
	if candidate == "" {
		return
	}

	// Record the auto-hop candidate
	c.peerConnsMu.Lock()
	pc, ok = c.peerConns[targetPeerID]
	if ok && pc.AutoHopVia == "" {
		pc.AutoHopVia = candidate
		c.peerConnsMu.Unlock()

		c.emit(EventAutoHopEstablished, LogEvent{
			Level:   "info",
			Message: fmt.Sprintf("Auto-hop: %s reachable via %s", shortID(targetPeerID), shortID(candidate)),
		})
		c.emit(EventLog, LogEvent{
			Level:   "info",
			Message: fmt.Sprintf("Auto-hop discovered: %s → %s → %s", shortID(c.MyID), shortID(candidate), shortID(targetPeerID)),
		})
	} else {
		c.peerConnsMu.Unlock()
	}
}

// cleanupPeerP2PMap removes a peer's entry from the P2P connectivity maps
// and clears any auto-hop routes that depended on that peer.
// Called when a peer disconnects.
func (c *Client) cleanupPeerP2PMap(peerID string) {
	c.p2pMapsMu.Lock()
	delete(c.p2pMaps, peerID)
	c.p2pMapsMu.Unlock()

	// Clear any auto-hop routes that used this peer as a hop
	c.peerConnsMu.Lock()
	for _, pc := range c.peerConns {
		if pc.AutoHopVia == peerID {
			pc.AutoHopVia = ""
			pc.AutoHopID = ""
		}
	}
	c.peerConnsMu.Unlock()
}
