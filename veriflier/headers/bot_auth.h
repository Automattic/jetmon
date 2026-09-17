
#ifndef __BOT_AUTH_H__
#define __BOT_AUTH_H__

#include <string>

// Web Bot Auth (RFC 9421 HTTP Message Signatures) support: Ed25519 signing
// of outgoing monitoring checks. Deliberately Qt-free so it can be compiled
// and unit-tested standalone (see test/bot-auth-veriflier.cpp).
namespace BotAuth {

	// Loads the Ed25519 private key used for signing. Call once at startup,
	// before checker threads start. Returns false on a bad key. When never
	// called (or the key was rejected), signing stays disabled.
	bool set_signing_key( const std::string &p_key_pem, const std::string &p_key_id, const std::string &p_agent_url );

	// Returns the Signature-Agent / Signature-Input / Signature headers for a
	// HEAD request to the given host and path (each ending in CRLF), or an
	// empty string when signing is disabled or signing fails.
	std::string signature_headers( const std::string &p_host, const std::string &p_path );

}

#endif // __BOT_AUTH_H__
