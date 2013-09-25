
#ifndef BUILDING_NODE_EXTENSION
#define BUILDING_NODE_EXTENSION
#endif

#include <iostream>
#include <string>
#include <unistd.h>

#include <node.h>

using namespace v8;
using namespace node;

#include "ping.h"
#include "http_checker.h"

struct Ping_Check_Baton {
	Persistent<Function> callback;
	Pinger *ping_checker;
	std::string server;
	int num_check;
	int num_pass;
	int server_id;
};

struct HTTP_Check_Baton {
	Persistent<Function> callback;
	HTTP_Checker *http_checker;
	std::string server;
	int port;
	int server_id;
};

void http_check_async_fin( uv_work_t *req, int status ) {
	HTTP_Check_Baton *baton = static_cast<HTTP_Check_Baton*>(req->data);
	Handle<Value> argv[3] = { Number::New( baton->server_id ),  Number::New( baton->http_checker->get_rtt() ),
								String::New( baton->http_checker->get_str_desc().c_str() ) };
	TryCatch try_catch;
	MakeCallback( Context::GetCurrent()->Global(), baton->callback, 3, argv);
	if ( try_catch.HasCaught() )
        FatalException( try_catch );

	baton->callback.Dispose();
	delete baton->http_checker;
	delete baton;
}

void http_check_async( uv_work_t *req ) {
	HTTP_Check_Baton *baton = static_cast<HTTP_Check_Baton*>(req->data);
	baton->http_checker->check( baton->server, baton->port );
}

static Handle<Value> http_check( const Arguments &args ) {
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

	Local<Function> cb = Local<Function>::Cast( args[3] );
	baton->callback = Persistent<Function>::New( cb );

	uv_work_t *req = new uv_work_t();
	req->data = baton;

	uv_queue_work( uv_default_loop(), req, http_check_async, (uv_after_work_cb)http_check_async_fin);

	return scope.Close( Undefined() );
}

void ping_check_async_fin( uv_work_t *req, int status ) {
	Ping_Check_Baton *baton = static_cast<Ping_Check_Baton*>(req->data);
	Handle<Value> argv[2] = { Number::New( baton->server_id ),  Number::New( baton->ping_checker->get_avg_usec() ) };

	TryCatch try_catch;
	MakeCallback( Context::GetCurrent()->Global(), baton->callback, 2, argv);
	if ( try_catch.HasCaught() )
        FatalException( try_catch );

	baton->callback.Dispose();
	delete baton->ping_checker;
	delete baton;
}

void ping_check_async( uv_work_t *req ) {
	Ping_Check_Baton *baton = static_cast<Ping_Check_Baton*>(req->data);
	baton->ping_checker->ping( baton->server, baton->num_check, baton->num_pass );
}

Handle<Value> ping_check( const Arguments& args ) {
	HandleScope scope;
	if (args.Length() < 5) {
		ThrowException( Exception::TypeError( String::New( "Wrong number of arguments" ) ) );
		return scope.Close( Undefined() );
	}

	if ( ! args[1]->IsNumber() || ! args[2]->IsNumber() ) {
		ThrowException( Exception::TypeError( String::New( "The number of packets and minimum packets arguments are not valid" ) ) );
		return scope.Close( Undefined() );
	}

	if ( ! args[3]->IsNumber() ) {
		ThrowException( Exception::TypeError( String::New( "The server id argument is not valid" ) ) );
		return scope.Close( Undefined() );
	}

	if ( ! args[4]->IsFunction() ) {
		ThrowException( Exception::TypeError( String::New( "You have not provided a callback function as the 5th parameter" ) ) );
		return scope.Close( Undefined() );
	}

	Ping_Check_Baton *baton = new Ping_Check_Baton();
	Pinger *o_ping = new Pinger();
	baton->ping_checker = o_ping;

	String::AsciiValue sHost( args[0]->ToString() );
	baton->server = *sHost;

	baton->num_check = args[1]->NumberValue();
	baton->num_pass = args[2]->NumberValue();
	baton->server_id = args[3]->NumberValue();

	Local<Function> cb = Local<Function>::Cast( args[4] );
	baton->callback = Persistent<Function>::New( cb );

	uv_work_t *req = new uv_work_t();
	req->data = baton;

	uv_queue_work( uv_default_loop(), req, ping_check_async, (uv_after_work_cb)ping_check_async_fin);

	return scope.Close( Undefined() );
}

void Initialise( Handle<Object> exports) {
	exports->Set( String::NewSymbol( "ping_check" ), FunctionTemplate::New( ping_check )->GetFunction() );
	exports->Set( String::NewSymbol( "http_check" ), FunctionTemplate::New( http_check )->GetFunction() );
}

NODE_MODULE(watcher, Initialise)

