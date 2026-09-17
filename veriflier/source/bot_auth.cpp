
#include "headers/bot_auth.h"

#include <openssl/evp.h>
#include <openssl/pem.h>

#include <ctime>

namespace {

	// How long a request signature stays valid after creation.
	const int SIGNATURE_EXPIRES_SEC = 300;

	// ponytail: set once at startup before checker threads run, then only
	// ever read - no locking needed. Never freed.
	EVP_PKEY   *g_key       = NULL;
	std::string g_key_id    = "";
	std::string g_agent_url = "";

}

bool BotAuth::set_signing_key( const std::string &p_key_pem, const std::string &p_key_id, const std::string &p_agent_url ) {
	BIO *bio = BIO_new_mem_buf( p_key_pem.data(), (int)p_key_pem.size() );
	if ( NULL == bio )
		return false;

	EVP_PKEY *key = PEM_read_bio_PrivateKey( bio, NULL, NULL, NULL );
	BIO_free( bio );

	if ( NULL == key )
		return false;

	if ( EVP_PKEY_ED25519 != EVP_PKEY_id( key ) ) {
		EVP_PKEY_free( key );
		return false;
	}

	g_key       = key;
	g_key_id    = p_key_id;
	g_agent_url = p_agent_url;
	return true;
}

std::string BotAuth::signature_headers( const std::string &p_host, const std::string &p_path ) {
	try {
		if ( NULL == g_key )
			return "";

		size_t q_pos = p_path.find_first_of( '?' );
		bool has_query = ( std::string::npos != q_pos );

		std::string s_components = "\"@method\" \"@authority\" \"@path\"";
		if ( has_query )
			s_components += " \"@query\"";
		s_components += " \"signature-agent\"";

		time_t created = time( NULL );
		std::string s_params = "(" + s_components + ")"
			+ ";created=" + std::to_string( created )
			+ ";keyid=\"" + g_key_id + "\""
			+ ";alg=\"ed25519\""
			+ ";expires=" + std::to_string( created + SIGNATURE_EXPIRES_SEC )
			+ ";tag=\"web-bot-auth\"";

		// RFC 9421 @method is case-sensitive: sign the method as sent (HEAD).
		// Signature-Agent is a quoted structured string, matching the
		// draft-meunier-http-message-signatures-directory-03 format that
		// Cloudflare's deployed verification requires.
		std::string s_agent_quoted = "\"" + g_agent_url + "\"";
		std::string s_base = "\"@method\": HEAD\n";
		s_base += "\"@authority\": " + p_host + "\n";
		s_base += "\"@path\": " + p_path.substr( 0, q_pos ) + "\n";
		if ( has_query )
			s_base += "\"@query\": " + p_path.substr( q_pos ) + "\n";
		s_base += "\"signature-agent\": " + s_agent_quoted + "\n";
		s_base += "\"@signature-params\": " + s_params;

		EVP_MD_CTX *md_ctx = EVP_MD_CTX_new();
		if ( NULL == md_ctx )
			return "";

		size_t sig_len = 0;
		bool ok = ( 1 == EVP_DigestSignInit( md_ctx, NULL, NULL, NULL, g_key ) )
			&& ( 1 == EVP_DigestSign( md_ctx, NULL, &sig_len, (const unsigned char*)s_base.data(), s_base.size() ) );

		std::string s_sig;
		if ( ok ) {
			s_sig.resize( sig_len );
			ok = ( 1 == EVP_DigestSign( md_ctx, (unsigned char*)&s_sig[0], &sig_len, (const unsigned char*)s_base.data(), s_base.size() ) );
		}
		EVP_MD_CTX_free( md_ctx );

		if ( ! ok )
			return "";

		std::string s_b64;
		s_b64.resize( 4 * ( ( sig_len + 2 ) / 3 ) + 1 );
		int b64_len = EVP_EncodeBlock( (unsigned char*)&s_b64[0], (const unsigned char*)s_sig.data(), (int)sig_len );
		s_b64.resize( b64_len > 0 ? b64_len : 0 );

		std::string s_headers = "Signature-Agent: " + s_agent_quoted + "\r\n";
		s_headers += "Signature-Input: sig1=" + s_params + "\r\n";
		s_headers += "Signature: sig1=:" + s_b64 + ":\r\n";
		return s_headers;
	}
	catch( ... ) {
		return "";
	}
}
