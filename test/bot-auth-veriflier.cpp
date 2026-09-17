
/**
 * Standalone check for the veriflier Web Bot Auth (RFC 9421) signing module.
 * The module is Qt-free on purpose, so this builds without a Qt toolchain:
 *
 *   g++ -std=c++11 -Iveriflier test/bot-auth-veriflier.cpp veriflier/source/bot_auth.cpp -lssl -lcrypto -o /tmp/bot-auth-veriflier && /tmp/bot-auth-veriflier
 */

#include "headers/bot_auth.h"

#include <openssl/evp.h>
#include <openssl/pem.h>

#include <cassert>
#include <cstring>
#include <iostream>
#include <string>

const std::string KEY_ID        = "test-key-1";
const std::string DIRECTORY_URL = "https://example.com/.well-known/http-message-signatures-directory";

static std::string get_header( const std::string &headers, const std::string &name ) {
	size_t start = headers.find( name + ": " );
	assert( start != std::string::npos );
	start += name.length() + 2;
	size_t end = headers.find( "\r\n", start );
	assert( end != std::string::npos );
	return headers.substr( start, end - start );
}

static std::string b64_decode( const std::string &b64 ) {
	std::string out;
	out.resize( b64.size() );
	int n = EVP_DecodeBlock( (unsigned char*)&out[0], (const unsigned char*)b64.data(), (int)b64.size() );
	assert( n > 0 );
	size_t pad = 0;
	if ( b64.size() > 0 && '=' == b64[ b64.size() - 1 ] ) pad++;
	if ( b64.size() > 1 && '=' == b64[ b64.size() - 2 ] ) pad++;
	out.resize( n - pad );
	return out;
}

// Rebuilds the signature base for the known inputs and verifies the signature.
static void verify_headers( const std::string &headers, EVP_PKEY *pubkey,
							const std::string &host, const std::string &path, bool expect_query ) {
	std::string sig_input = get_header( headers, "Signature-Input" );
	std::string sig_line  = get_header( headers, "Signature" );
	std::string sig_agent = get_header( headers, "Signature-Agent" );

	// Signature-Agent is a quoted structured string (Cloudflare's required
	// draft-meunier-http-message-signatures-directory-03 form).
	assert( sig_agent == "\"" + DIRECTORY_URL + "\"" );
	assert( 0 == sig_input.compare( 0, 5, "sig1=" ) );

	std::string params = sig_input.substr( 5 );
	assert( params.find( "keyid=\"" + KEY_ID + "\"" ) != std::string::npos );
	assert( params.find( "alg=\"ed25519\"" ) != std::string::npos );
	assert( params.find( "tag=\"web-bot-auth\"" ) != std::string::npos );
	assert( ( params.find( "\"@query\"" ) != std::string::npos ) == expect_query );

	size_t c_pos = params.find( ";created=" );
	size_t e_pos = params.find( ";expires=" );
	assert( c_pos != std::string::npos && e_pos != std::string::npos );
	long created = atol( params.c_str() + c_pos + 9 );
	long expires = atol( params.c_str() + e_pos + 9 );
	assert( 300 == expires - created );

	size_t q_pos = path.find_first_of( '?' );
	std::string base = "\"@method\": HEAD\n";
	base += "\"@authority\": " + host + "\n";
	base += "\"@path\": " + path.substr( 0, q_pos ) + "\n";
	if ( std::string::npos != q_pos )
		base += "\"@query\": " + path.substr( q_pos ) + "\n";
	base += "\"signature-agent\": \"" + DIRECTORY_URL + "\"\n";
	base += "\"@signature-params\": " + params;

	assert( 0 == sig_line.compare( 0, 6, "sig1=:" ) );
	assert( ':' == sig_line[ sig_line.size() - 1 ] );
	std::string sig = b64_decode( sig_line.substr( 6, sig_line.size() - 7 ) );
	assert( 64 == sig.size() );

	EVP_MD_CTX *vctx = EVP_MD_CTX_new();
	assert( NULL != vctx );
	assert( 1 == EVP_DigestVerifyInit( vctx, NULL, NULL, NULL, pubkey ) );
	int ok = EVP_DigestVerify( vctx, (const unsigned char*)sig.data(), sig.size(),
								(const unsigned char*)base.data(), base.size() );
	EVP_MD_CTX_free( vctx );
	assert( 1 == ok );
}

int main() {
	// 1. Disabled by default.
	assert( BotAuth::signature_headers( "example.com", "/" ).empty() );

	// 2. Bad key material is rejected.
	assert( ! BotAuth::set_signing_key( "not a pem", KEY_ID, DIRECTORY_URL ) );
	assert( BotAuth::signature_headers( "example.com", "/" ).empty() );

	// 3. Generate an Ed25519 keypair and export the private key as PEM.
	EVP_PKEY_CTX *kctx = EVP_PKEY_CTX_new_id( EVP_PKEY_ED25519, NULL );
	assert( NULL != kctx );
	assert( 1 == EVP_PKEY_keygen_init( kctx ) );
	EVP_PKEY *pkey = NULL;
	assert( 1 == EVP_PKEY_keygen( kctx, &pkey ) );
	EVP_PKEY_CTX_free( kctx );

	BIO *pem_bio = BIO_new( BIO_s_mem() );
	assert( 1 == PEM_write_bio_PrivateKey( pem_bio, pkey, NULL, NULL, 0, NULL, NULL ) );
	char *pem_data = NULL;
	long pem_len = BIO_get_mem_data( pem_bio, &pem_data );
	std::string pem( pem_data, pem_len );
	BIO_free( pem_bio );

	// 4. Valid key: headers are produced and verify against the public key.
	assert( BotAuth::set_signing_key( pem, KEY_ID, DIRECTORY_URL ) );

	std::string headers = BotAuth::signature_headers( "example.com", "/signed/path?x=1&y=2" );
	assert( ! headers.empty() );
	verify_headers( headers, pkey, "example.com", "/signed/path?x=1&y=2", true );

	// 5. Path without a query string: no @query component.
	headers = BotAuth::signature_headers( "example.com", "/" );
	verify_headers( headers, pkey, "example.com", "/", false );

	EVP_PKEY_free( pkey );

	std::cout << "veriflier bot-auth signing test: OK" << std::endl;
	return 0;
}
