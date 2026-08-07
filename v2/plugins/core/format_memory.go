package core

// format_memory.go — a renderização do bloco <anhur-memory>. Separado do core.go
// pela regra do corte da casa (2026-08-07): este arquivo é a VOZ do plugin — tudo
// que o modelo lê no início da sessão nasce aqui, e mais nada.

import (
	"fmt"
	"strings"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// Junior Tip [profile pode ser nil, DE PROPÓSITO, 2026-07-31]: quando o AnhurDB está
// inalcançável não há perfil para renderizar — mas é exatamente aí que os avisos
// (backlog, quarentena, redundância local) importam mais. Um nil aqui significa
// "monte só o que der", e o retorno vazio significa "não havia nada a dizer".
// Quem chama nunca imprime string vazia, então o recall continua sem bloquear o
// início da sessão.
func formatMemory(cfg config, profile *client.ProfileResult, backlog queueBacklog, archive archiveHealth) string {
	var builder strings.Builder
	builder.WriteString(`<anhur-memory backend="AnhurDB" container="` + cfg.container + `">` + "\n")
	if profile != nil {
		builder.WriteString("You (Claude) have persistent long-term memory in AnhurDB. This is what you remember — trust it, build on it, and keep it accurate (use supersede when a fact changes).\n")
	} else {
		builder.WriteString("AnhurDB could not be reached at session start, so NONE of your long-term memory was recalled. What follows is only what this plugin can see locally.\n")
	}

	// Junior Tip [the warning goes ABOVE the memory, not below it, 2026-07-17]: a stuck queue means
	// turns that never reached AnhurDB, so everything rendered below is knowingly incomplete. The
	// model must read that caveat BEFORE it reads (and acts on) the recalled content, and must tell
	// the user — the user has no other way to learn about it, since the queue only fails into a log
	// file. This is the whole point of routing the backlog here instead of leaving it in plugin.log.
	if backlog.chunkCount > 0 {
		builder.WriteString("\n## ⚠️ Unpersisted backlog — the memory below is INCOMPLETE\n")
		builder.WriteString(fmt.Sprintf("- %d chunk(s) of past conversation are stuck on disk and never reached AnhurDB (oldest queued %s).\n",
			backlog.chunkCount, backlog.oldestChunk))
		if backlog.lastFlushErr != nil {
			builder.WriteString(fmt.Sprintf("- Last retry failed with: %v\n", backlog.lastFlushErr))
		}
		builder.WriteString("- They are NOT lost (retried on every persist), but they will not appear in recall until they drain.\n")
		builder.WriteString("- TELL THE USER about this now, in your first reply: they cannot see it otherwise.\n")
	}

	// Junior Tip [quarantine outranks the backlog, 2026-07-30]: a stuck chunk drains by itself
	// the moment AnhurDB recovers; a quarantined chunk NEVER does — it has no provable session,
	// and writing it anywhere else would merge two conversations (forbidden: 1 Claude session =
	// 1 AnhurDB session). Only a human can resolve it, and this block is the only channel this
	// plugin owns that reliably reaches one.
	if backlog.quarantinedChunkCount > 0 {
		builder.WriteString("\n## 🚨 Quarantined chunks — memory below is missing turns that will NOT self-heal\n")
		builder.WriteString(fmt.Sprintf("- %d chunk(s) of past conversation had no identifiable session and were moved to the queue's quarantine/ directory (oldest queued %s).\n",
			backlog.quarantinedChunkCount, backlog.oldestQuarantined))
		builder.WriteString("- They are NEVER persisted automatically: writing them into another conversation's session would merge sessions (1 Claude session = 1 AnhurDB session, inviolable).\n")
		builder.WriteString("- TELL THE USER about this now, in your first reply: only a human can decide where these chunks belong.\n")
	}

	// A redundância local vem DEPOIS do backlog e da quarentena (aqueles são turnos
	// concretos que não chegaram; este é a rede que os pegaria) e ANTES da memória
	// recuperada, pela mesma razão de sempre: a ressalva tem de ser lida antes do
	// conteúdo sobre o qual ela incide.
	renderArchiveWarning(&builder, archive)

	if profile == nil {
		// Sem perfil não há seções nem estatísticas. Se também não houve aviso
		// nenhum, devolver vazio impede um bloco que só diz "não consegui" —
		// ruído a cada início de sessão offline.
		if !backlogNeedsAttention(backlog) && !archive.degraded() {
			return ""
		}
		builder.WriteString("\n(No memory was recalled this session. The warnings above are what this plugin could determine without AnhurDB.)\n")
		builder.WriteString("</anhur-memory>")
		return builder.String()
	}

	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		if len(items) > cfg.recallLimit {
			items = items[:cfg.recallLimit]
		}
		builder.WriteString("\n## " + title + "\n")
		for _, item := range items {
			builder.WriteString("- " + item + "\n")
		}
	}

	// Junior Tip [6 satélites + 1 curinga + 3 tópicos — decisão do dono, 2026-08-06]:
	// o SERVIDOR agora cura o perfil (service/profile.go): um item por tipo, o mais
	// pesado, mais um curinga com o vice-campeão global. Este renderizador só imprime
	// o que chegar — a regra tem UM dono e as três portas (este plugin, o provider do
	// Hermes, o REST) herdam juntas. Risks e Emotions existiam no banco desde sempre
	// e nunca apareciam aqui: a extração pagava LLM para identificar riscos e o canal
	// automático os ignorava. O curinga vem primeiro: é o item mais pesado sem assento,
	// e o topo do bloco é a posição que o modelo lê com mais atenção.
	section("Highlight", stringList(profile.Static, "highlight"))
	section("Decisions", stringList(profile.Static, "decisions"))
	section("Facts", stringList(profile.Static, "facts"))
	section("Preferences", stringList(profile.Static, "preferences"))
	section("Risks", stringList(profile.Static, "risks"))
	section("Open tasks", stringList(profile.Dynamic, "recent_tasks"))
	section("Emotions", stringList(profile.Static, "emotions"))
	section("Recent topics", stringList(profile.Dynamic, "recent_topics"))

	total := numField(profile.Stats, "total_records")
	sessions := numField(profile.Stats, "sessions")
	// Junior Tip [do not advertise tools the model cannot call, 2026-07-17]: this line used to end
	// with "The MCP tools mcp__anhurdb__* let you recall/store more during this session." That was
	// false, and expensively so — it is injected into the model's context EVERY session, so it acted
	// as a standing instruction to reach for tools that always fail. Every mcp__anhurdb__* tool takes
	// api_key as a REQUIRED argument (server-side: auth.APIKeyParam is mcp.Required() and
	// GetAPIKeyFromArgs reads it from tool args only — the Bearer header is a perimeter gate, not
	// tenant auth). The key deliberately lives ONLY in the 0600 env file, never in the transcript,
	// precisely because the Stop hook persists that transcript INTO AnhurDB — advertising these tools
	// invites the model to leak the key into the memory the key protects. Say what is true instead:
	// the loop is automatic and the model is not in it.
	builder.WriteString(fmt.Sprintf("\n(%d records across %d sessions. This block was injected automatically at session start, and your turns are persisted automatically — you do not invoke either, and you cannot call the mcp__anhurdb__* tools: they require an api_key that is deliberately kept out of your context.)\n", total, sessions))
	builder.WriteString("</anhur-memory>")
	return builder.String()
}

func stringList(section map[string]interface{}, key string) []string {
	rawList, ok := section[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
			out = append(out, str)
		}
	}
	return out
}

// numField reads a numeric stat that JSON decoded as float64.
func numField(stats map[string]interface{}, key string) int {
	if f, ok := stats[key].(float64); ok {
		return int(f)
	}
	return 0
}
