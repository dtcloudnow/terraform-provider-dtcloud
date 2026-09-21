resource "dtcloud_lb_listener" "https" {
  lb_id         = dtcloud_lb.public.id
  name          = "https"
  protocol      = "HTTPS"
  protocol_port = 443

  # Only these addresses may reach the listener.
  allowed_cidrs = ["10.0.0.0/8"]

  # Milliseconds here. dtcloud_lb_balancing_pool takes the same three in seconds.
  timeout_client_data = 50000
}
