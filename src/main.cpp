
#ifndef BUILDING_NODE_EXTENSION
#define BUILDING_NODE_EXTENSION
#endif

#include <iostream>
#include <string>
#include <unistd.h>

#include <node.h>

using namespace v8;
using namespace node;

#include "http_checker.h"

struct HTTP_Check_Baton {
	Persistent<Function> callback;
	HTTP_Checker *http_checker;
	std::string server;
	int port;
	int server_id;
};

void http_check_async_fin( uv_work_t *req, int status ) {
	HandleScope scope;
	HTTP_Check_Baton *baton = static_cast<HTTP_Check_Baton*>(req->data);
	Handle<Value> argv[3] = { Number::New( baton->server_id ),  Number::New( baton->http_checker->get_rtt() ),
								Number::New( baton->http_checker->get_response_code() ) };
	TryCatch try_catch;
	MakeCallback( Context::GetCurrent()->Global(), baton->callback, 3, argv );
	if ( try_catch.HasCaught() )
		FatalException( try_catch );

	baton->callback.Dispose();
	baton->callback.Clear();
	delete baton->http_checker;
	delete baton;
	delete req;
}

void http_check_async( uv_work_t *req ) {
	HTTP_Check_Baton *baton = static_cast<HTTP_Check_Baton*>( req->data );
	baton->http_checker->check( baton->server, baton->port );
}

Handle<Value> http_check( const Arguments &args ) {
	HandleScope scope;
	if ( args.Length() < 4 ) {
		ThrowException( Exception::TypeError( String::New( "Wrong number of arguments" ) ) );
		return scope.Close( Undefined() );
	}

	if ( ! args[1]->IsNumber() ) {
		ThrowException( Exception::TypeError( String::New( "The port number argument is not valid" ) ) );
		return scope.Close( Undefined() );
	}

	if ( ! args[2]->IsNumber() ) {
		ThrowException( Exception::TypeError( String::New( "The server id argument is not valid" ) ) );
		return scope.Close( Undefined() );
	}

	if ( ! args[3]->IsFunction() ) {
		ThrowException( Exception::TypeError( String::New( "You have not provided a callback function as the 4th parameter" ) ) );
		return scope.Close( Undefined() );
	}

	HTTP_Check_Baton *baton = new HTTP_Check_Baton();
	HTTP_Checker *checker = new HTTP_Checker();
	baton->http_checker = checker;

	String::AsciiValue sHost( args[0]->ToString() );
	baton->server = *sHost;

	baton->port = args[1]->NumberValue();
	baton->server_id = args[2]->NumberValue();

	baton->callback = Persistent<Function>::New( args[3].As<Function>() );

	uv_work_t *req = new uv_work_t();
	req->data = baton;

	uv_queue_work( uv_default_loop(), req, http_check_async, (uv_after_work_cb)http_check_async_fin );

	return Undefined();
}

void Initialise( Handle<Object> exports) {
	HandleScope scope;

	SSL_load_error_strings();
	SSL_library_init();
	OpenSSL_add_all_algorithms();

	exports->Set( String::NewSymbol( "http_check" ), FunctionTemplate::New( http_check )->GetFunction() );
}

NODE_MODULE( jetmon, Initialise )

