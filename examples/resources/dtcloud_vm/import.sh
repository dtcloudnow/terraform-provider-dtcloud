# The boot-time arguments (key_name, user_data, script, is_gpu_image) are not
# reported by any endpoint, so an imported machine takes them from the
# configuration rather than from the platform.
terraform import dtcloud_vm.web 4c9a1f37-8d25-4e60-b13f-6a7c2e5b9d88
