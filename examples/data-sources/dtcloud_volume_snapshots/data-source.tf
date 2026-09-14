data "dtcloud_volume_snapshots" "of_data_volume" {
  volume_id = dtcloud_volume.data.id
}
