package bedrock

// Middleware wraps a Handler and returns a new Handler.
// Middleware can intercept requests before they reach the handler,
// modify the context, short-circuit the request, or wrap the response.
type Middleware func(Handler) Handler

// Chain builds a middleware chain that executes in the order provided.
// The middlewares are applied right-to-left so they execute left-to-right.
//
// Example:
//
//	Chain(handler, logging, auth, rateLimit)
//	Execution order: logging -> auth -> rateLimit -> handler
func Chain(handler Handler, middlewares ...Middleware) Handler {
	// Start with the final handler
	final := handler

	// Wrap in reverse order so they execute in the order provided
	for i := len(middlewares) - 1; i >= 0; i-- {
		final = middlewares[i](final)
	}

	return final
}
