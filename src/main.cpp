
#ifndef BUILDING_NODE_EXTENSION
#define BUILDING_NODE_EXTENSION
#endif

#include <iostream>
#include <string>
#include <unistd.h>

#include <node.h>
#include <uv.h>

using namespace v8;
using namespace node;

#include "http_checker.h"

struct HTTP_Check_Baton {
	CopyablePersistentTraits<Function>::CopyablePersistent callback;
	HTTP_Checker *http_checker;
	std::string server;
	int port;
	int server_id;
};

static void http_check_async_fin( uv_work_t *req, int status ) {
	Isolate* isolate = Isolate::GetCurrent();
	HandleScope scope( isolate );

	HTTP_Check_Baton *baton = static_cast<HTTP_Check_Baton*>(req->data);
	Local<Value> argv[3] = { Number::New( isolate, baton->server_id ),
								Number::New( isolate, baton->http_checker->get_rtt() ),
								Number::New( isolate, baton->http_checker->get_response_code() ) };

	Local<Function> cb_func = Local<Function>::New( isolate, baton->callback );
	cb_func->Call( isolate->GetCurrentContext(), isolate->GetCurrentContext()->Global(), 3, argv );
	baton->callback.Reset();
	delete baton->http_checker;
	delete baton;
	delete req;
}

void http_check_async( uv_work_t *req ) {
	HTTP_Check_Baton *baton = static_cast<HTTP_Check_Baton*>( req->data );
	baton->http_checker->check( baton->server, baton->port );
}

void http_check( const FunctionCallbackInfo<Value>& args ) {
	args.GetReturnValue().SetUndefined();
	Isolate* isolate = args.GetIsolate();
	HandleScope scope( isolate );

	if ( args.Length() < 4 ) {
		isolate->ThrowException( Exception::TypeError(
			String::NewFromUtf8( isolate, "Wrong number of arguments" ).ToLocalChecked() ) );
		return;
	}

	if ( ! args[1]->IsNumber() ) {
		isolate->ThrowException( Exception::TypeError(
			String::NewFromUtf8( isolate, "The port number argument is not valid" ).ToLocalChecked() ) );
		return;
	}

	if ( ! args[2]->IsNumber() ) {
		isolate->ThrowException( Exception::TypeError(
			String::NewFromUtf8( isolate, "The server id argument is not valid" ).ToLocalChecked() ) );
		return;
	}

	if ( ! args[3]->IsFunction() ) {
		isolate->ThrowException( Exception::TypeError(
			String::NewFromUtf8( isolate, "You have not provided a callback function as the 4th parameter" ).ToLocalChecked() ) );
		return;
	}

	HTTP_Check_Baton *baton = new HTTP_Check_Baton();
	HTTP_Checker *checker = new HTTP_Checker();
	baton->http_checker = checker;

	String::Utf8Value sHost( isolate, args[0] );
	baton->server = *sHost;

	baton->port = args[1]->ToInteger( isolate->GetCurrentContext() ).ToLocalChecked()->Value();
	baton->server_id = (int) args[2]->ToInteger( isolate->GetCurrentContext() ).ToLocalChecked()->Value();

	CopyablePersistentTraits<Function>::CopyablePersistent percy( isolate, args[3].As<Function>() );
	baton->callback.Reset( isolate, percy );

	uv_work_t *req = new uv_work_t();
	req->data = baton;

	uv_queue_work( uv_default_loop(), req, http_check_async, (uv_after_work_cb)http_check_async_fin );
}

void Initialise( Local<Object> exports) {
	SSL_load_error_strings();
	SSL_library_init();
	OpenSSL_add_all_algorithms();
#if (SSLEAY_VERSION_NUMBER >= 0x0907000L)
	OPENSSL_config( NULL );
#endif

	NODE_SET_METHOD( exports, "http_check", http_check );
}

NODE_MODULE( jetmon, Initialise )

