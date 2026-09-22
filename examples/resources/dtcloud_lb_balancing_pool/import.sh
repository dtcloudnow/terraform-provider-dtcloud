# <load-balancer-id>:<balancing-pool-id>. Import one only when it declares no
# `member` blocks: a member's port is reported by nothing, so declared members
# come back empty and the first plan proposes a rebuild.
terraform import dtcloud_lb_balancing_pool.web 3d372446-4c75-4327-9511-98442456162f:5b2a9c31-7e04-4d88-9c6e-44a2f1d0e7b3
