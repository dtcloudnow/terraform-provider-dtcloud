resource "dtcloud_lb_health_monitor" "web" {
  lb_id   = dtcloud_lb.public.id
  pool_id = dtcloud_lb_pool.web.pool_id

  # A pool can carry only one monitor; a second one is refused.
  type     = "HTTP"
  url_path = "/healthz"

  interval            = 10
  timeout             = 5
  healthy_threshold   = 2
  unhealthy_threshold = 3
}
