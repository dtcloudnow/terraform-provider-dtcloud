resource "dtcloud_lb_pool" "web" {
  lb_id       = dtcloud_lb.public.id
  listener_id = dtcloud_lb_listener.https.listener_id

  backend_protocol      = "HTTP"
  backend_protocol_port = 8080
  lb_algorithm          = "ROUND_ROBIN"

  # Pin a client to the member it first reached.
  sticky_session = true
}
