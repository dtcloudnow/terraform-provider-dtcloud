# The whole chain in one call: listener, pool, health monitor and members.
# Every argument is ForceNew -- changing one rebuilds all four objects.
resource "dtcloud_lb_balancing_pool" "web" {
  lb_id = dtcloud_lb.public.id

  # Front end
  protocol      = "HTTP"
  protocol_port = 80

  # Back end
  backend_protocol      = "HTTP"
  backend_protocol_port = 8080
  lb_algorithm          = "ROUND_ROBIN"

  # Health monitor. `protocol` here is the probe type, not a network protocol.
  health_monitor {
    protocol            = "HTTP"
    url_path            = "/healthz"
    interval            = 10
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  member {
    compute_server_id = dtcloud_vm.web.id
    address           = dtcloud_vm.web.primary_ip
    protocol_port     = 8080
  }
}
