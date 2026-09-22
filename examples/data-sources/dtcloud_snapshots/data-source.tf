data "dtcloud_snapshots" "ready" {
  status = "available"
}

data "dtcloud_snapshots" "of_data_volume" {
  volume_id = dtcloud_volume.data.id
}
