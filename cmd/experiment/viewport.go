package main

func (c *controller) focusVerification(r *resources) {
	view, err := r.runner.VerificationViewport(r.chrome.Context())
	if err != nil {
		c.log("未能读取验证框位置", map[string]any{"原因": err.Error()})
	}
	if err := r.remote.SetViewport(view); err != nil {
		c.log("验证框位置不在画面内", nil)
		return
	}
	c.mu.Lock()
	if c.res == r {
		c.state.Viewport = view
	}
	c.mu.Unlock()
}
