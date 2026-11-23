# Supported Ubuntu images, newest first.
data "h3_vm_images" "ubuntu" {
  family = "ubuntu"
  status = "supported"
}

output "latest_ubuntu" {
  value = data.h3_vm_images.ubuntu.images[0].id
}
